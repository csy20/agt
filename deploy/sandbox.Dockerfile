# Fixed image for per-run test execution. The model never supplies the command line.
FROM python:3.12-alpine
CMD ["sleep", "infinity"]
