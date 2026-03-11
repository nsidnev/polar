import dramatiq

from polar.subscription.tasks import scan_renewal_reminders  # noqa: F401

if __name__ == "__main__":
    dramatiq.get_broker().get_actor("subscription.scan_renewal_reminders").send()
