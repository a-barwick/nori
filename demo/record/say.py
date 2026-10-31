"""Send one real message to the hosted thread: python3 say.py "message"."""
import sys
import uuid

from lib import call

r = call("turn", {"id": str(uuid.uuid4()), "message": sys.argv[1]})
print("accepted", len(r.get("messages", [])), r.get("conversation", {}).get("pending"), r.get("conversation", {}).get("routing_blocked"))
