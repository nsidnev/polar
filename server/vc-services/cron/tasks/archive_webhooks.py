import dramatiq

from polar.webhook.tasks import webhook_event_archive  # noqa: F401

if __name__ == "__main__":
    dramatiq.get_broker().get_actor("webhook_event.archive").send()
