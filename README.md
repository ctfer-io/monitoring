<div align="center">
  <h1>Monitoring</h1>
  <a href="https://pkg.go.dev/github.com/ctfer-io/monitoring"><img src="https://shields.io/badge/-reference-blue?logo=go&style=for-the-badge" alt="reference"></a>
  <a href="https://goreportcard.com/report/github.com/ctfer-io/monitoring"><img src="https://goreportcard.com/badge/github.com/ctfer-io/monitoring?style=for-the-badge" alt="go report"></a>
  <a href="https://coveralls.io/github/ctfer-io/monitoring?branch=main"><img src="https://img.shields.io/coverallsCoverage/github/ctfer-io/monitoring?style=for-the-badge" alt="Coverage Status"></a>
  <br>
  <a href=""><img src="https://img.shields.io/github/license/ctfer-io/monitoring?style=for-the-badge" alt="License"></a>
  <a href="https://github.com/ctfer-io/monitoring/actions?query=workflow%3Aci+"><img src="https://img.shields.io/github/actions/workflow/status/ctfer-io/monitoring/ci.yaml?style=for-the-badge&label=CI" alt="CI"></a>
  <a href="https://github.com/ctfer-io/monitoring/actions/workflows/codeql-analysis.yaml"><img src="https://img.shields.io/github/actions/workflow/status/ctfer-io/monitoring/codeql-analysis.yaml?style=for-the-badge&label=CodeQL" alt="CodeQL"></a>
  <br>
  <a href="https://securityscorecards.dev/viewer/?uri=github.com/ctfer-io/monitoring"><img src="https://img.shields.io/ossf-scorecard/github.com/ctfer-io/monitoring?label=openssf%20scorecard&style=for-the-badge" alt="OpenSSF Scoreboard"></a>
</div>

The _Monitoring_ component is in charge of the collection, process and storage of various signals (i.e. logs, metrics and distributed traces).
It is extensively based upon [OpenTelemetry](https://opentelemetry.io/), with its [Collector](https://github.com/open-telemetry/opentelemetry-collector).

> [!CAUTION]
>
> This component is an **internal** work mostly used for development purposes.
> It is used for production purposes too, i.e. on Capture The Flag events.
>
> Nonetheless, **we do not include it in the repositories we are actively maintaining**.

## Architecture

The Monitoring service's architecture currently provides:
- [OpenTelemetry Collector](https://opentelemetry.io/docs/collector/)
- [Jaeger UI](https://www.jaegertracing.io/)
- [Prometheus](https://prometheus.io/)
- [Perses](https://perses.dev/)

The multiple parts passes information in a non-cyclic way to avoid deadlocks (DAG or dependencies), as summarized in the following diagram.

<div align="center">
    <img src="res/architecture.excalidraw.png" alt="The architecture of the Monitoring service and its parts">
</div>

## Cold Extract

For research and/or development purposes, the architecture provide way to perform an extraction of the OpenTelemetry data.

1. Activate cold extract:
  ```bash
  pulumi config set cold-extract true
  ```

2. Use the Monitoring through the OTEL Collector endpoint.

3. Run the extractor:
  ```bash
  mkdir extract
  go run cmd/extractor/main.go \
    --namespace $(pulumi stack export namespace) \
    --pvc-name $(pulumi stack export otel-cold-extract-pvc-name) \
    --directory extract
  ```

## TODO list

- Add AlertManager (require Prometheus)
- Add ElasticSearch/OpenSearch for logs and/or storage backend for traces
