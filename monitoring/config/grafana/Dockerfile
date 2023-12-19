FROM grafana/grafana:latest

# Path: dashboards/dashboard.yml
COPY dashboard-config.yaml /etc/grafana/provisioning/dashboards/dashboard.yaml

# Path: dashboards/*.json
COPY dashboards/* /etc/dashboards/

# Path: grafana.ini
COPY grafana.ini /etc/grafana/grafana.ini
