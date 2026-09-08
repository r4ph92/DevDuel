FROM node:22-alpine

# Nothing is installed. The API uses only node:http, and the tests use the
# fetch built into Node 22, so this image needs no network at build time and
# none at judge time either.
WORKDIR /app
