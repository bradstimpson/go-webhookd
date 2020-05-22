bump-version:
	perl -i -p -e 's/github.com\/whosonfirst\/go-webhookd\/$(PREVIOUS)/github.com\/whosonfirst\/go-webhookd\/$(NEW)/g' go.mod
	perl -i -p -e 's/github.com\/whosonfirst\/go-webhookd\/$(PREVIOUS)/github.com\/whosonfirst\/go-webhookd\/$(NEW)/g' README.md
	find . -name '*.go' | xargs perl -i -p -e 's/github.com\/whosonfirst\/go-webhookd\/$(PREVIOUS)/github.com\/whosonfirst\/go-webhookd\/$(NEW)/g'

tools:
	go build -mod vendor -o bin/webhookd cmd/webhookd/main.go
	go build -mod vendor -o bin/webhookd-test-github cmd/webhookd-test-github/main.go
	go build -mod vendor -o bin/webhookd-generate-hook cmd/webhookd-generate-hook/main.go

debug:
	bin/webhookd -config ./config.json

hook:
	./bin/webhookd-generate-hook
