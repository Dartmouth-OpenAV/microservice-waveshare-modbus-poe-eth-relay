# Build stage: compiles and tests the microservice with the full Go toolchain
FROM golang:latest AS build

COPY source /go/src

ENV GOPATH=

WORKDIR /go/src/microservice-framework
RUN go mod init github.com/Dartmouth-OpenAV/microservice-framework
RUN go mod tidy

WORKDIR /go
# Change the module path for each microservice
RUN go mod init github.com/Dartmouth-OpenAV/microservice-waveshare-relay
RUN go mod edit -replace github.com/Dartmouth-OpenAV/microservice-framework=./src/microservice-framework
RUN go mod tidy

WORKDIR /go/src
RUN go get -u
# Unit tests run against a simulated Waveshare relay, so no hardware is needed at build time
RUN go vet . && go test .
# Static binary (no libc needed) with the time zone database embedded, since the framework
# timestamps its logs in America/New_York and the final image has no /usr/share/zoneinfo
RUN CGO_ENABLED=0 go build -tags timetzdata -trimpath -ldflags="-s -w" -o /go/bin/microservice

# Final stage: just the binary
FROM scratch

COPY --from=build /go/bin/microservice /microservice

EXPOSE 80

# Exec form is required since there's no shell in a scratch image
ENTRYPOINT ["/microservice"]
