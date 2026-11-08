# Nori

Keep production Nori available. Do not pause production or stop its computer for testing or cleanup. Use the local stack or sandbox for checks that require a shutdown. Use `make pause` only when Austin requests it. An authorized production release may pause briefly; restore availability once the release or rollback is verified, unless production was already paused. Leave the browser VM's normal idle stop and on-demand wake in place.
