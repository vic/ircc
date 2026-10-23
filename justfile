build:
    nix-shell -p go --run 'go build -o ircc ./cmd/ircc'

test:
    nix-shell -p go --run 'go test ./...'

fmt:
    nix-shell -p go --run 'go fmt ./...'

vet:
    nix-shell -p go --run 'go vet ./...'
