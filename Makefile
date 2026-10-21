IMAGE ?= slincsilver/real-time-ui-update-microservice
VERSION ?= 1.0.1
PLATFORMS ?= linux/amd64,linux/arm64
GOVULNCHECK ?= golang.org/x/vuln/cmd/govulncheck@v1.9.0
SOURCE_DATE_EPOCH ?= $(shell git log -1 --format=%ct)
BUILD_ARGS = --build-arg SOURCE_DATE_EPOCH=$(SOURCE_DATE_EPOCH)

.PHONY: test e2e e2e-short e2e-image perf bench scan docker-build docker-push

test:
	go test -race ./...

e2e:
	go test -tags e2e -count=1 ./test/e2e/

e2e-short:
	go test -tags e2e -count=1 -short ./test/e2e/

e2e-image: docker-build
	E2E_IMAGE=$(IMAGE):$(VERSION) go test -tags e2e -count=1 -short ./test/e2e/

perf:
	go test -tags e2e -count=1 -run TestPerformance -v ./test/e2e/

bench:
	go test -run=^$$ -bench=. -benchmem ./...

# Fails on any vulnerability reachable from our code, and on any fixable
# critical or high vulnerability anywhere in the image.
scan: docker-build
	go run $(GOVULNCHECK) ./...
	docker scout cves --exit-code --only-fixed --only-severity critical,high local://$(IMAGE):$(VERSION)

docker-build:
	docker buildx build $(BUILD_ARGS) --load -t $(IMAGE):$(VERSION) -t $(IMAGE):latest .

docker-push: scan
	docker buildx build $(BUILD_ARGS) --platform $(PLATFORMS) --sbom=true --provenance=mode=max \
		--output type=image,\"name=$(IMAGE):$(VERSION),$(IMAGE):latest\",push=true,rewrite-timestamp=true .
