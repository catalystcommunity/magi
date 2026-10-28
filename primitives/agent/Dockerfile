# Build from the repository root:
#   docker build -f primitives/agent/Dockerfile -t magi-agent .
FROM python:3.13-slim

ENV PYTHONDONTWRITEBYTECODE=1 PYTHONUNBUFFERED=1 PYTHONPATH=/app:/app/clients
WORKDIR /app
COPY clients/python/magi_workflow /app/clients/magi_workflow
COPY clients/python/magi_execution /app/clients/magi_execution
COPY clients/python/magi_artifacts /app/clients/magi_artifacts
COPY clients/python/magi_rpc.py /app/clients/magi_rpc.py
COPY primitives/agent/magi_agent /app/magi_agent

RUN useradd --uid 10001 --create-home --shell /usr/sbin/nologin magi \
 && python -c "import magi_agent.worker"
USER 10001
ENTRYPOINT ["python", "-m", "magi_agent"]
CMD ["worker"]
