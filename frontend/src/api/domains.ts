import { delay, uid, domains as store } from './_mock'

export interface Domain {
  id: string
  service_id: string
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
  list: async (_projectId: string, serviceId: string): Promise<Domain[]> => {
    await delay()
    return (store[serviceId] ?? []) as Domain[]
  },

  add: async (_projectId: string, serviceId: string, data: AddDomainPayload): Promise<Domain> => {
    await delay(500)
    const entry: Domain = {
      id: `dom-${uid()}`,
      service_id: serviceId,
      domain: data.domain,
      tls: data.tls ?? true,
      verified: false,
      created_at: new Date().toISOString(),
      updated_at: new Date().toISOString(),
    }
    if (!store[serviceId]) store[serviceId] = []
    store[serviceId].push(entry)
    return entry
  },

  delete: async (_projectId: string, serviceId: string, domainId: string): Promise<void> => {
    await delay()
    if (store[serviceId]) {
      const i = (store[serviceId] as Domain[]).findIndex((d) => d.id === domainId)
      if (i !== -1) store[serviceId].splice(i, 1)
    }
  },

  verify: async (_projectId: string, serviceId: string, domainId: string): Promise<{ verified: boolean; resolved_ip: string; server_ip: string }> => {
    await delay(1200)
    const dom = (store[serviceId] ?? []).find((d) => (d as Domain).id === domainId) as Domain | undefined
    // Simulate 50% chance of verification success for design purposes
    const success = Math.random() > 0.4
    if (dom && success) {
      dom.verified = true
      dom.updated_at = new Date().toISOString()
    }
    return {
      verified: success,
      resolved_ip: success ? '203.0.113.42' : '1.2.3.4',
      server_ip: '203.0.113.42',
    }
  },
}
