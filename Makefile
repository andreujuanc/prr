.PHONY: coverage coverage-html

coverage:
	go test -coverprofile=coverage.out -covermode=atomic ./...
	go tool cover -func=coverage.out

coverage-html: coverage
	go tool cover -html=coverage.out -o coverage.html
	@echo "Coverage report generated: coverage.html"
