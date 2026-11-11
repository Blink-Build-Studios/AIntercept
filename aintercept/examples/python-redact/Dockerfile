# The Python example Interceptor (docs/BUILDING_AN_INTERCEPTOR.md). Build from
# the aintercept/ directory so the proto is in the context:
#   docker build -f examples/python-redact/Dockerfile -t my-interceptor aintercept/
FROM python:3.13-slim
WORKDIR /app
RUN pip install --no-cache-dir grpcio==1.71.0 grpcio-tools==1.71.0 protobuf==5.29.3
COPY interceptor/v1/interceptor.proto proto/interceptor/v1/interceptor.proto
RUN python -m grpc_tools.protoc -I proto --python_out=. --grpc_python_out=. proto/interceptor/v1/interceptor.proto \
 && touch interceptor/__init__.py interceptor/v1/__init__.py
COPY examples/python-redact/server.py .
USER nobody
EXPOSE 9100
CMD ["python", "server.py"]
