FROM python:3.12-slim

ENV PYTHONUNBUFFERED=True

RUN rm -rf /etc/apt/apt.conf/docker-clean

COPY --link ./requirements.txt /assistant/requirements.txt
RUN --mount=type=cache,target=/root/.cache/pip pip install -r /assistant/requirements.txt

COPY --link . /assistant

ENTRYPOINT ["python", "/assistant/__main__.py"]
