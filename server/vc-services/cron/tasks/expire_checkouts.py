import dramatiq

from polar.checkout.tasks import expire_open_checkouts  # noqa: F401

if __name__ == "__main__":
    dramatiq.get_broker().get_actor("checkout.expire_open_checkouts").send()
