FROM golang:1.14

WORKDIR /go/src/potentials-utils
COPY . .

EXPOSE 8080
CMD ["sh", "bin/start.sh"]
