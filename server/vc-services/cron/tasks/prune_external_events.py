import dramatiq

from polar.external_event.tasks import external_event_prune  # noqa: F401

if __name__ == "__main__":
    dramatiq.get_broker().get_actor("external_event.prune").send()
