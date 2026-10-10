FROM golang:1.27-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 go build -trimpath -o /terrarium ./cmd/terrarium

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /terrarium /terrarium
ENTRYPOINT ["/terrarium"]
