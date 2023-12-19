FROM grafana/grafana:latest

# Path: dashboards/dashboard.yml
COPY provisioning/dashboards/dashboard.yml /etc/grafana/provisioning/dashboards/dashboard.yml

# Path: dashboards/*.json
COPY provisioning/dashboards/*.json /etc/dashboards/

# Path: grafana.ini
COPY grafana.ini /etc/grafana/grafana.ini
