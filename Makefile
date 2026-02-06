compile:
	docker run --rm -v $(shell pwd):/workspace -w /workspace rvolosatovs/protoc \
		--proto_path=. \
		--go_out=. \
		--go-grpc_out=. \
		--go_opt=paths=source_relative \
		--go-grpc_opt=paths=source_relative \
		api/v1/*.proto

test:
	CGO_ENABLED=1 go test -race ./...
