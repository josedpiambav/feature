FROM golang:1.24-alpine AS builder
WORKDIR /app
COPY . .
RUN CGO_ENABLED=0 go build -o /feature-branching

FROM gcr.io/distroless/static-debian12
COPY --from=builder /feature-branching /
ENTRYPOINT ["/feature-branching"]