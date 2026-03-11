import dramatiq

from polar.auth.tasks import auth_delete_expired  # noqa: F401

if __name__ == "__main__":
    dramatiq.get_broker().get_actor("auth.delete_expired").send()
