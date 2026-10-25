data "terraform_remote_state" "foundation" {
  count   = var.enabled ? 1 : 0
  backend = "local"
  config = {
    path = pathexpand(var.foundation_state_path)
  }
}

data "google_compute_disk" "retained" {
  count   = var.enabled ? 1 : 0
  project = "bot-111549"
  zone    = "us-central1-a"
  name    = "bot-computer-data"
}

# Read only while enabling the runtime, so cleanup works after the deadline.
data "external" "deadline" {
  count   = var.enabled ? 1 : 0
  program = ["python3", "${path.module}/../deadline.py"]
  query = {
    termination_time = var.termination_time
  }
}

resource "google_compute_instance" "host" {
  count                     = var.enabled ? 1 : 0
  project                   = "bot-111549"
  zone                      = "us-central1-a"
  name                      = var.host_name
  machine_type              = "n2-standard-2"
  min_cpu_platform          = "Intel Cascade Lake"
  allow_stopping_for_update = false
  tags                      = ["nori-computer"]
  labels = {
    purpose    = "bot-persistent-computer"
    lifecycle  = "disposable"
    managed_by = "terraform"
  }

  advanced_machine_features {
    enable_nested_virtualization = true
  }

  boot_disk {
    auto_delete = true
    initialize_params {
      image = "projects/ubuntu-os-cloud/global/images/family/ubuntu-2404-lts-amd64"
      type  = "pd-standard"
      size  = 20
    }
  }

  # Attached disks are retained by this resource. Only the boot disk auto-deletes.
  attached_disk {
    source      = data.terraform_remote_state.foundation[0].outputs.retained_disk_id
    device_name = "nori-guest-data"
    mode        = "READ_WRITE"
  }

  network_interface {
    subnetwork = data.terraform_remote_state.foundation[0].outputs.subnetwork_id
    # Ephemeral public IPv4 provides guest egress; SSH ingress is IAP-only.
    access_config {}
  }

  # Omitting service_account sends no serviceAccounts to the GCE insert API.
  # Do not add an empty block: that would select the project default account.
  scheduling {
    automatic_restart           = false
    on_host_maintenance         = "TERMINATE"
    provisioning_model          = "STANDARD"
    instance_termination_action = "DELETE"
    termination_time            = var.termination_time
  }

  metadata = {
    block-project-ssh-keys = "true"
    enable-oslogin         = "false"
    serial-port-enable     = "false"
    startup-script         = file("${path.module}/../../persistent-host.sh")
    shutdown-script        = "#!/bin/bash\nset -e\n[ ! -x /usr/local/sbin/nori-vm ] || /usr/local/sbin/nori-vm stop\n"
    nori-public-key        = trimspace(file(pathexpand(var.public_key_path)))
    nori-build-guest       = file("${path.module}/../../build-guest.sh")
    nori-vm-control        = file("${path.module}/../../vm-control.sh")
    nori-browser-close     = file("${path.module}/../../browser-close.py")
    nori-fresh-disk        = "false"
  }

  lifecycle {
    precondition {
      condition     = data.external.deadline[0].result.valid == "true"
      error_message = "Host DELETE deadline must be future and within 30 minutes."
    }
    precondition {
      condition     = data.terraform_remote_state.foundation[0].outputs.retained_disk_numeric_id == "3757334953750232874"
      error_message = "Foundation must reference the original retained disk."
    }
    precondition {
      condition     = data.google_compute_disk.retained[0].disk_id == "3757334953750232874"
      error_message = "The live retained disk identity changed. Refusing to attach a replacement with the same name."
    }
    precondition {
      condition     = can(regex("^(ssh-ed25519|ssh-rsa|ecdsa-sha2-nistp[0-9]+) [A-Za-z0-9+/=]+( .*)?$", trimspace(file(pathexpand(var.public_key_path)))))
      error_message = "public_key_path must contain one OpenSSH public key, never a private key."
    }
    postcondition {
      condition     = length(self.service_account) == 0
      error_message = "The disposable host must have no attached service account."
    }
  }
}
