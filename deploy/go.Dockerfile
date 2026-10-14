# Go profile image. Dependencies are the toolchain in the image. GOPROXY stays off.
FROM golang:1.23-alpine
# Numeric user for hosts that cannot supply a uid (native Windows Getuid is -1).
# Unix hosts override this with --user <host-uid>:<host-gid>.
RUN adduser -D -u 65532 -H agt
CMD ["sleep", "infinity"]
