COMPOSE_INTEGRATION := docker compose -f compose.integration.yaml

# Start the ephemeral emulator stack (LocalStack S3 + Azurite Blob +
# fake-gcs-server) and wait for healthchecks. Services are left running until
# `make integration-down`.
integration-up:
	$(COMPOSE_INTEGRATION) up -d --wait --wait-timeout 120

# Run the emulator-backed integration suite. Services stay up afterwards so
# repeated runs are fast; use `make integration-down` to stop them.
test-integration: integration-up
	CLOUDKIT_INTEGRATION=1 go test -race -count=1 -timeout=5m -v ./tests/integration

# Stop and remove the integration compose project (containers only).
integration-down:
	$(COMPOSE_INTEGRATION) down

.PHONY: integration-up test-integration integration-down
