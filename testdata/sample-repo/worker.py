from acme_sdk import AcmeClient
from legacy_store import Store

client = AcmeClient("py-secret")
store = Store()

item = client.get_item("item_42")
cached = store.get_item("item_42")
