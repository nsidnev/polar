import dramatiq

from polar.customer_session.tasks import customer_session_delete_expired  # noqa: F401

if __name__ == "__main__":
    dramatiq.get_broker().get_actor("customer_session.delete_expired").send()
