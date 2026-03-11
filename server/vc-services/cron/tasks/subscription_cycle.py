import dramatiq

from polar.subscription.tasks import subscription_cycle_due  # noqa: F401

if __name__ == "__main__":
    dramatiq.get_broker().get_actor("subscription.cycle_due").send()
