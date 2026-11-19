# A host with systemd as PID 1 and OpenSSH, for the detached runner's
# systemd tests (test/systemd). Not a production image.
FROM debian:bookworm-slim
RUN apt-get update \
 && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
      systemd systemd-sysv dbus dbus-user-session libpam-systemd openssh-server procps \
 && rm -rf /var/lib/apt/lists/*
# deploy has lingering (its user manager outlives SSH sessions); nolinger
# does not, so the runner must fall back to setsid for it.
RUN useradd -m -s /bin/sh deploy && useradd -m -s /bin/sh nolinger \
 && mkdir -p /var/lib/systemd/linger && touch /var/lib/systemd/linger/deploy \
 && sed -i 's/^#\?PasswordAuthentication .*/PasswordAuthentication no/' /etc/ssh/sshd_config \
 && systemctl enable ssh \
 && systemctl mask systemd-firstboot.service getty@.service console-getty.service
STOPSIGNAL SIGRTMIN+3
CMD ["/sbin/init"]
