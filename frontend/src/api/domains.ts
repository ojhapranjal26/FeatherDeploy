import client from './client'

export interface Domain {
  id: number
  service_id: number
  domain: string
  tls: boolean
  verified: boolean
  created_at: string
  updated_at: string
}

export interface AddDomainPayload {
  domain: string
  tls?: boolean
}

export const domainsApi = {
  list: (projectId: string | number, serviceId: string | number): Promise<Domain[]> =>
    client.get<Domain[]>(`/projects/${projectId}/services/${serviceId}/domains`).then((r) => r.data),

  add: (projectId: string | number, serviceId: string | number, data: AddDomainPayload): Promise<Domain> =>
    client.post<Domain>(`/projects/${projectId}/services/${serviceId}/domains`, data).then((r) => r.data),

  delete: (projectId: string | number, serviceId: string | number, domainId: string | number): Promise<void> =>
    client.delete(`/projects/${projectId}/services/${serviceId}/domains/${domainId}`).then(() => undefined),

  verify: (
    projectId: string | number,
    serviceId: string | number,
    domainId: string | number
  ): Promise<{ verified: boolean; resolved_ip: string; server_ip: string }> =>
    client
      .post<{ verified: boolean; resolved_ip: string; server_ip: string }>(
        `/projects/${projectId}/services/${serviceId}/domains/${domainId}/verify`
      )
      .then((r) => r.data),
}
