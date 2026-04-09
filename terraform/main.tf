# =============================================================================
# Taller 1 - Construcción de pipelines en Cloud
# Integrantes: Heiner Rincón - Mariana De La Cruz
# Infraestructura: Azure Kubernetes Service (AKS) + Azure Container Registry (ACR)
# =============================================================================

terraform {
  required_version = ">= 1.5.0"

  required_providers {
    azurerm = {
      source  = "hashicorp/azurerm"
      version = "~> 3.0"
    }
  }

  backend "azurerm" {
    resource_group_name  = "rg-microservices-demo"
    storage_account_name = "stmicrosvcdemo2025"
    container_name       = "tfstate"
    key                  = "microservices-demo.tfstate"
  }
}

provider "azurerm" {
  features {}
}

# =============================================================================
# VARIABLES
# =============================================================================
# VARIABLES
variable "resource_group_name" {
  description = "Nombre del resource group principal"
  type        = string
  default     = "rg-microservices-demo"
}

variable "location" {
  description = "Región de Azure"
  type        = string
  default     = "canadacentral"
}

variable "environment" {
  description = "Ambiente de despliegue"
  type        = string
  default     = "dev"
}

variable "project_name" {
  description = "Nombre del proyecto"
  type        = string
  default     = "microservices-demo"
}

variable "aks_node_count" {
  description = "Número de nodos del cluster AKS"
  type        = number
  default     = 2
}

variable "aks_node_vm_size" {
  description = "Tamaño de VM para los nodos de AKS"
  type        = string
  default     = "Standard_B2s"
}

#variable "kubernetes_version" {
 #description = "Versión de Kubernetes"
  #type        = string
  #default     = "1.29.101"
#}

# =============================================================================
# LOCALS
# =============================================================================

locals {
  common_tags = {
    project     = var.project_name
    environment = var.environment
    managed_by  = "terraform"
  }

  acr_name = replace(lower("acr${var.project_name}${var.environment}"), "-", "")
  aks_name = "aks-${var.project_name}-${var.environment}"
}

# =============================================================================
# RESOURCE GROUP
# =============================================================================

resource "azurerm_resource_group" "main" {
  name     = var.resource_group_name
  location = var.location
  tags     = local.common_tags
}

# =============================================================================
# AZURE CONTAINER REGISTRY (ACR)
# =============================================================================

resource "azurerm_container_registry" "acr" {
  name                = local.acr_name
  resource_group_name = azurerm_resource_group.main.name
  location            = azurerm_resource_group.main.location
  sku                 = "Basic"
  admin_enabled       = false
  tags                = local.common_tags
}

# =============================================================================
# VIRTUAL NETWORK
# =============================================================================

resource "azurerm_virtual_network" "main" {
  name                = "vnet-${var.project_name}-${var.environment}"
  address_space       = ["10.0.0.0/16"]
  location            = azurerm_resource_group.main.location
  resource_group_name = azurerm_resource_group.main.name
  tags                = local.common_tags
}

resource "azurerm_subnet" "aks" {
  name                 = "subnet-aks"
  resource_group_name  = azurerm_resource_group.main.name
  virtual_network_name = azurerm_virtual_network.main.name
  address_prefixes     = ["10.0.1.0/24"]
}

# =============================================================================
# AZURE KUBERNETES SERVICE (AKS)
# =============================================================================

resource "azurerm_kubernetes_cluster" "aks" {
  name                = local.aks_name
  location            = azurerm_resource_group.main.location
  resource_group_name = azurerm_resource_group.main.name
  dns_prefix          = "${var.project_name}-${var.environment}"
  #kubernetes_version  = var.kubernetes_version

  default_node_pool {
    name           = "default"
    node_count     = var.aks_node_count
    vm_size        = var.aks_node_vm_size
    vnet_subnet_id = azurerm_subnet.aks.id

    node_labels = {
      role = "general"
    }

    upgrade_settings {
      max_surge = "10%"
    }
  }

  identity {
    type = "SystemAssigned"
  }

  network_profile {
    network_plugin    = "azure"
    network_policy    = "azure"
    service_cidr      = "10.1.0.0/16"
    dns_service_ip    = "10.1.0.10"
  }

  tags = local.common_tags
}

# =============================================================================
# INTEGRACIÓN AKS ↔ ACR
# =============================================================================

resource "azurerm_role_assignment" "aks_acr_pull" {
  principal_id                     = azurerm_kubernetes_cluster.aks.kubelet_identity[0].object_id
  role_definition_name             = "AcrPull"
  scope                            = azurerm_container_registry.acr.id
  skip_service_principal_aad_check = true
}

# =============================================================================
# OUTPUTS
# =============================================================================

output "resource_group_name" {
  description = "Nombre del resource group"
  value       = azurerm_resource_group.main.name
}

output "acr_login_server" {
  description = "URL del ACR para hacer docker push/pull"
  value       = azurerm_container_registry.acr.login_server
}

output "acr_name" {
  description = "Nombre del ACR"
  value       = azurerm_container_registry.acr.name
}

output "aks_cluster_name" {
  description = "Nombre del cluster AKS"
  value       = azurerm_kubernetes_cluster.aks.name
}

output "aks_kube_config" {
  description = "Kubeconfig para conectarse al cluster"
  value       = azurerm_kubernetes_cluster.aks.kube_config_raw
  sensitive   = true
}