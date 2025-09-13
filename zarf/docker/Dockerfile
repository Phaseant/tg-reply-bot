ARG GO_BASE_VERSION=1.24.2
ARG GO_BASE_IMAGE=alpine

FROM golang:${GO_BASE_VERSION}-${GO_BASE_IMAGE} AS build
ARG SERVICE_NAME=service

WORKDIR /src

# Download dependencies
COPY ./go.mod ./go.sum ./

COPY . .

# Build
RUN go build -o ./${SERVICE_NAME} ./cmd/main.go

# Run api
FROM alpine AS main
ARG SERVICE_NAME=service

COPY --from=build --chmod=777 --chown=nobody:nogroup /src/${SERVICE_NAME} ./app/${SERVICE_NAME}
COPY --from=build --chmod=777 /src/session ./app/session
COPY --from=build --chmod=777 /src/.env ./app/.env

USER nobody:nogroup
WORKDIR /app

CMD ["./service"]