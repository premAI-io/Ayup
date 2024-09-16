FROM python:3.12-slim

ENV PYTHONUNBUFFERED=True

COPY --link --chmod=555 . /assistant

ENTRYPOINT ["/assistant/cmd"]
