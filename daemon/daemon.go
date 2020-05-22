package daemon

import (
	"context"
	"errors"
	"fmt"
	"github.com/aaronland/go-http-server"
	"github.com/whosonfirst/go-webhookd/v2"
	"github.com/whosonfirst/go-webhookd/v2/config"
	"github.com/whosonfirst/go-webhookd/v2/dispatchers"
	"github.com/whosonfirst/go-webhookd/v2/receivers"
	"github.com/whosonfirst/go-webhookd/v2/transformations"
	"github.com/whosonfirst/go-webhookd/v2/webhook"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"
)

type WebhookDaemon struct {
	server     server.Server
	webhooks   map[string]webhookd.WebhookHandler
	AllowDebug bool
}

func NewWebhookDaemonFromConfig(ctx context.Context, cfg *config.WebhookConfig) (*WebhookDaemon, error) {

	server_uri := fmt.Sprintf("%s://%s:%", cfg.Daemon.Protocol, cfg.Daemon.Host, cfg.Daemon.Port)

	d, err := NewWebhookDaemon(ctx, server_uri)

	if err != nil {
		return nil, err
	}

	err = d.AddWebhooksFromConfig(ctx, cfg)

	if err != nil {
		return nil, err
	}

	d.AllowDebug = cfg.Daemon.AllowDebug

	return d, nil
}

func NewWebhookDaemon(ctx context.Context, server_uri string) (*WebhookDaemon, error) {

	srv, err := server.NewServer(ctx, server_uri)

	if err != nil {
		return nil, err
	}

	webhooks := make(map[string]webhookd.WebhookHandler)

	d := WebhookDaemon{
		server:     srv,
		webhooks:   webhooks,
		AllowDebug: false,
	}

	return &d, nil
}

func (d *WebhookDaemon) AddWebhooksFromConfig(ctx context.Context, cfg *config.WebhookConfig) error {

	if len(cfg.Webhooks) == 0 {
		return errors.New("No webhooks defined")
	}

	for i, hook := range cfg.Webhooks {

		if hook.Endpoint == "" {
			msg := fmt.Sprintf("Missing endpoint at offset %d", i+1)
			return errors.New(msg)
		}

		if hook.Receiver == "" {
			msg := fmt.Sprintf("Missing receiver at offset %d", i+1)
			return errors.New(msg)
		}

		if len(hook.Dispatchers) == 0 {
			msg := fmt.Sprintf("Missing dispatchers at offset %d", i+1)
			return errors.New(msg)
		}

		receiver_config, err := cfg.GetReceiverConfigByName(hook.Receiver)

		if err != nil {
			return err
		}

		receiver, err := receivers.NewReceiverFromConfig(ctx, receiver_config)

		if err != nil {
			return err
		}

		var steps []webhookd.WebhookTransformation

		for _, name := range hook.Transformations {

			transformation_config, err := cfg.GetTransformationConfigByName(name)

			if err != nil {
				return err
			}

			step, err := transformations.NewTransformationFromConfig(ctx, transformation_config)

			if err != nil {
				return err
			}

			steps = append(steps, step)
		}

		var sendto []webhookd.WebhookDispatcher

		for _, name := range hook.Dispatchers {

			dispatcher_config, err := cfg.GetDispatcherConfigByName(name)

			if err != nil {
				return err
			}

			dispatcher, err := dispatchers.NewDispatcherFromConfig(ctx, dispatcher_config)

			if err != nil {
				return err
			}

			sendto = append(sendto, dispatcher)
		}

		wh, err := webhook.NewWebhook(hook.Endpoint, receiver, steps, sendto)

		if err != nil {
			return err
		}

		err = d.AddWebhook(wh)

		if err != nil {
			return err
		}

	}

	return nil
}

func (d *WebhookDaemon) AddWebhook(wh webhook.Webhook) error {

	endpoint := wh.Endpoint()
	_, ok := d.webhooks[endpoint]

	if ok {
		return errors.New("endpoint already configured")
	}

	d.webhooks[endpoint] = wh
	return nil
}

func (d *WebhookDaemon) HandlerFunc() (http.HandlerFunc, error) {

	handler := func(rsp http.ResponseWriter, req *http.Request) {

		ctx := req.Context()

		ctx, cancel := context.WithCancel(ctx)
		defer cancel()

		endpoint := req.URL.Path

		wh, ok := d.webhooks[endpoint]

		if !ok {
			http.Error(rsp, "404 Not found", http.StatusNotFound)
			return
		}

		t1 := time.Now()

		var ta time.Time
		var tb time.Duration

		var ttr time.Duration // time to receive
		var ttt time.Duration // time to transform
		var ttd time.Duration // time to dispatch

		ta = time.Now()

		rcvr := wh.Receiver()

		body, err := rcvr.Receive(ctx, req)

		// we use -1 to signal that this is an unhandled event but
		// not an error, for example when github sends a ping message
		// (20190212/thisisaaronland)

		if err != nil && err.Code == -1 {
			return
		}

		if err != nil {
			http.Error(rsp, err.Error(), err.Code)
			return
		}

		tb = time.Since(ta)

		ttr = tb

		ta = time.Now()

		for _, step := range wh.Transformations() {

			body, err = step.Transform(ctx, body)

			if err != nil {
				http.Error(rsp, err.Error(), err.Code)
				return
			}

			// check to see if there is anything left the transformation
			// https://github.com/whosonfirst/go-webhookd/v2/issues/7
		}

		tb = time.Since(ta)
		ttt = tb

		// check to see if there is anything to dispatch
		// https://github.com/whosonfirst/go-webhookd/v2/issues/7

		ta = time.Now()

		wg := new(sync.WaitGroup)
		ch := make(chan *webhookd.WebhookError)

		for _, d := range wh.Dispatchers() {

			wg.Add(1)

			go func(d webhookd.WebhookDispatcher, body []byte) {

				defer wg.Done()

				err = d.Dispatch(ctx, body)

				if err != nil {
					ch <- err
				}

			}(d, body)
		}

		// https://github.com/whosonfirst/go-webhookd/issues/14
		// this is broken as in len(errors) will always be zero even if
		// there are errors (20190214/thisisaaronland)

		errors := make([]string, 0)

		go func() {

			for e := range ch {
				errors = append(errors, e.Error())
			}
		}()

		wg.Wait()

		if len(errors) > 0 {

			msg := strings.Join(errors, "\n\n")
			http.Error(rsp, msg, http.StatusInternalServerError)
			return
		}

		tb = time.Since(ta)
		ttd = tb

		t2 := time.Since(t1)

		rsp.Header().Set("X-Webhookd-Time-To-Receive", fmt.Sprintf("%v", ttr))
		rsp.Header().Set("X-Webhookd-Time-To-Transform", fmt.Sprintf("%v", ttt))
		rsp.Header().Set("X-Webhookd-Time-To-Dispatch", fmt.Sprintf("%v", ttd))
		rsp.Header().Set("X-Webhookd-Time-To-Process", fmt.Sprintf("%v", t2))

		if d.AllowDebug {

			query := req.URL.Query()
			debug := query.Get("debug")

			if debug != "" {
				rsp.Header().Set("Content-Type", "text/plain")
				rsp.Header().Set("Access-Control-Allow-Origin", "*")
				rsp.Write(body)
			}
		}

		return
	}

	return http.HandlerFunc(handler), nil
}

func (d *WebhookDaemon) Start(ctx context.Context) error {

	handler, err := d.HandlerFunc()

	if err != nil {
		return err
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", handler)

	svr := d.server

	log.Printf("webhookd listening for requests on %s\n", svr.Address())

	err = svr.ListenAndServe(ctx, mux)

	if err != nil {
		return err
	}

	return nil
}
