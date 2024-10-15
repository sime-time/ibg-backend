# Use the official Golang image as a parent image
FROM golang:latest

# Set the working directory inside the container
WORKDIR /app

# Copy the current directory contents into the container at /app
COPY . .

# Download any dependencies
RUN go mod download

# Make port 8090 available to the world outside this container
EXPOSE 8090

# Run the Go application when the container launches
CMD ["go", "run", "main.go", "serve"]