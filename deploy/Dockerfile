# Build: docker build -f deploy/Dockerfile --build-arg SERVICE=order .
FROM golang:1.26-alpine AS build
WORKDIR /src
ENV CGO_ENABLED=0
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG SERVICE
RUN go build -o /out/app ./services/${SERVICE}/cmd/server

FROM alpine:3.20
RUN adduser -D -u 10001 app
COPY --from=build /out/app /app
USER app
ENTRYPOINT ["/app"]
