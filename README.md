# Monitoring

The `monitoring` MicroService is in charge of the collection, process and storage of various signals (i.e. logs, metrics and distributed traces).
It is extensively based upon [OpenTelemetry](https://opentelemetry.io/), with its [Collector](https://github.com/open-telemetry/opentelemetry-collector).

## Architecture

<div align="center">
    <img src="architecture.excalidraw.png" alt="The Architecture of parts in the monitoring MicroService">
</div>

## TODO list

- Add Grafana (require Prometheus)
- Add AlertManager (require Prometheus)
- Think of Dashboard as Code, and if it is relevant to extract them (so technically how ? With Docker manifests inspection ? Service discovery and custom protocol ?) automatically ?
