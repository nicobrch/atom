BUNDLE_PLATFORM ?= $(shell go env GOOS)/$(shell go env GOARCH)

bundle:
	go tool bundler -output cmd/atom -platform $(BUNDLE_PLATFORM)

build: bundle
	go build -o atom ./cmd/atom

# Install Gitleaks from https://github.com/gitleaks/gitleaks, then scan all
# reachable Git history before pushing. CI runs the same scan on every push/PR.
secrets:
	gitleaks git --redact --config .gitleaks.toml .
