FROM alpine:3

# Installed at build time because judge time has no network at all. This is
# the rule every challenge image lives by, in its smallest possible form.
RUN apk add --no-cache busybox-extras
