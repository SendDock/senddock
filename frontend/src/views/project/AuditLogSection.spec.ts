import { describe, it, expect, vi, beforeEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'

const route = { params: { id: 'the-id' } }
vi.mock('vue-router', () => ({
    useRoute: () => route,
}))

const apiMock = vi.fn((_url: string) => Promise.resolve({ entries: [], total: 0 }))
vi.mock('@/api/client', () => ({
    api: (url: string) => apiMock(url),
}))

vi.mock('@/stores/license', () => ({
    useLicenseStore: () => ({ fetch: vi.fn(() => Promise.resolve()), allowsPro: true }),
}))

import AuditLogSection from './AuditLogSection.vue'

async function mountAuditLog(props: Record<string, unknown> = {}) {
    const wrapper = mount(AuditLogSection, { props })
    await flushPromises()
    return wrapper
}

function requestedURL(): string {
    const calls = apiMock.mock.calls
    expect(calls.length).toBeGreaterThan(0)
    return calls[calls.length - 1]?.[0] ?? ''
}

beforeEach(() => {
    apiMock.mockClear()
})

// The trail being read follows the scope. A workspace action has no project to be filed
// under, so reading it from the project endpoint would show an audit log with none of the
// workspace entries in it.
describe('AuditLogSection', () => {
    it('reads the project trail by default', async () => {
        await mountAuditLog()

        expect(requestedURL()).toContain('/projects/the-id/audit-log')
    })

    it('reads the workspace trail when scoped to a workspace', async () => {
        await mountAuditLog({ scope: 'workspace' })

        expect(requestedURL()).toContain('/workspaces/the-id/audit-log')
    })

    it('offers the workspace actions for the workspace trail', async () => {
        const wrapper = await mountAuditLog({ scope: 'workspace' })
        const optionValues = wrapper.findAll('option').map(o => (o.element as HTMLOptionElement).value)

        expect(optionValues).toContain('workspace.member_added')
        expect(optionValues).not.toContain('broadcast.send')
    })

    it('offers the project actions for the project trail', async () => {
        const wrapper = await mountAuditLog()
        const optionValues = wrapper.findAll('option').map(o => (o.element as HTMLOptionElement).value)

        expect(optionValues).toContain('broadcast.send')
        expect(optionValues).not.toContain('workspace.member_added')
    })
})
