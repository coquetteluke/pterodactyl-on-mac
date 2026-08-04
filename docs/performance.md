# Performance: native versus a VM

Part of [Pterodactyl on Mac](../README.md).

## Is it actually faster than a VM?

Mostly no. The win is memory, not speed.

These are measured on one machine, a 2019 Intel MacBook Pro with 16 GB running a
Paper 26.2 server and 43 plugins, comparing the same server before and after
it was moved off a Lima VM (8 vCPU, 12 GiB, `vz`) onto the host.

| | VM | native |
| --- | --- | --- |
| server startup | 39.7s (n=16) | ~38.6s |
| cold read, 130 MB / 244 files | 257 ms | 220 ms |
| memory reserved | 12 GiB | what the server uses (5.5 GB) |
| LAN round trip | ~0.4 ms | ~0.4 ms |

**Startup time does not improve.** Apple's Virtualization.framework is
hardware-accelerated, so CPU-bound JVM work already ran at close to native
speed. Sixteen boots from the VM era averaged 39.7s; the quietest native boot
was 38.6s. That is a wash, and it is the honest answer to "will my server run
faster": it will not, noticeably.

**Disk I/O improves by around 14%**, which is the virtio-blk and disk-image
layers going away. Worth having for a Minecraft server, which does a lot of
small random reads, but not transformative.

Beware of measuring this badly: dropping caches *inside* the guest does not
evict the disk image from the *host's* page cache, so the VM's "cold" read is
served from host RAM and looks about twice as fast as native. Both layers have
to be purged for the comparison to mean anything.

**Memory is the real difference.** A VM reserves what you give it whether the
workload needs it or not. 12 GiB of a 16 GiB machine was committed to running a
process that fits in 5.5 GB. Getting that back is the reason to do this, and on
a machine with plenty of RAM to spare the case is much weaker.

**Networking is unchanged** if the VM was already bridged. Lima's default
userspace port forwarding costs real latency, roughly 31 ms as observed here
before bridging, but `socket_vmnet` removes that without leaving the VM.

The operational difference does not show up in a benchmark: the VM is another
thing that has to be running. Backups on this node silently stopped for two
days because the VM was powered off, not because anything failed.
