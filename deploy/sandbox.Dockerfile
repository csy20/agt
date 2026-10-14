# Fixed image for per-run test execution. The model never supplies the command line.
FROM python:3.12-alpine
# Numeric user for hosts that cannot supply a uid (native Windows Getuid is -1).
# Unix hosts override this with --user <host-uid>:<host-gid>.
RUN adduser -D -u 65532 -H agt
CMD ["sleep", "infinity"]
