FROM golang:1.21-alpine AS builder

# Set the working directory inside the container
WORKDIR /app

# Copy go.mod and go.sum files to leverage Docker cache for dependencies
COPY go.mod go.sum ./

# Download Go module dependencies
RUN go mod download

# Copy the entire source code into the container
COPY . .

# Build the Go application
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-w -s" -o /bin/docdb-autoscaler ./cmd/main.go

# Stage 2: Create the final lightweight image
FROM alpine:latest

# Install CA certificates for HTTPS communication
RUN apk --no-cache add ca-certificates

# Create a non-root user and group named 'appuser'
RUN adduser -D appuser

# Switch to the non-root user
USER appuser

# Set the working directory inside the container
WORKDIR /app

# Copy the compiled binary from the builder stage
COPY --from=builder /bin/docdb-autoscaler /app/

# Specify the entrypoint for the container
CMD ["./docdb-autoscaler"]
