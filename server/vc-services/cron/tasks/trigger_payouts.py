import dramatiq

from polar.payout.tasks import trigger_stripe_payouts  # noqa: F401

if __name__ == "__main__":
    dramatiq.get_broker().get_actor("payout.trigger_stripe_payouts").send()
