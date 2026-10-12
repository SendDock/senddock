import { describe, it, expect, vi, beforeEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import type { Project } from '@/stores/projects'

const apiMock = vi.fn<(_url: string, _opts?: Record<string, unknown>) => Promise<unknown>>(
    (_url: string, _opts?: Record<string, unknown>) => Promise.resolve({ webhooks: [] })
)
vi.mock('@/api/client', () => ({
    api: (url: string, opts?: Record<string, unknown>) => apiMock(url, opts),
}))

import WebhooksSection from './WebhooksSection.vue'

const projectStub: Project = {
    id: 'proj-1',
    name: 'Test Project',
    description: '',
    from_name: 'Sender',
    from_email: 'sender@example.test',
    smtp_host: '127.0.0.1',
    smtp_port: 2525,
    smtp_user: 'user',
    smtp_allow_insecure_tls: false,
    created_at: '2026-01-01T00:00:00Z',
    updated_at: '2026-01-01T00:00:00Z',
}

describe('WebhooksSection', () => {
    beforeEach(() => {
        setActivePinia(createPinia())
        apiMock.mockReset()
        apiMock.mockResolvedValue({ webhooks: [] })
    })

    it('renders the newsletter unsubscribe event on existing webhooks', async () => {
        apiMock.mockResolvedValueOnce({
            webhooks: [
                {
                    id: 'wh-1',
                    url: 'https://example.com/hook',
                    secret: '',
                    events: ['subscriber.newsletter_unsubscribed'],
                    active: true,
                    created_at: '2026-09-01T00:00:00Z',
                },
            ],
        })

        const wrapper = mount(WebhooksSection, {
            props: { project: projectStub },
            global: {
                stubs: { Teleport: true },
            },
        })
        await flushPromises()

        expect(wrapper.text()).toContain('subscriber.newsletter_unsubscribed')
    })

    it('preselects all 8 webhook events including subscriber.newsletter_unsubscribed on create', async () => {
        const wrapper = mount(WebhooksSection, {
            props: { project: projectStub },
            global: {
                stubs: { Teleport: true },
            },
        })
        await flushPromises()

        const newBtn = wrapper.findAll('button').find(b => b.text().includes('New webhook'))
        expect(newBtn).toBeDefined()
        await newBtn!.trigger('click')

        expect(wrapper.text()).toContain('subscriber.newsletter_unsubscribed')
        expect(wrapper.text()).toContain('Fired when a subscriber opts out of a single newsletter')

        apiMock.mockResolvedValueOnce({
            id: 'wh-new',
            url: 'https://example.com/new',
            secret: 'a'.repeat(64),
            events: [],
            active: true,
            created_at: '2026-09-01T00:00:00Z',
        })

        const urlInput = wrapper.find('input[placeholder*="https://"]')
        await urlInput.setValue('https://example.com/new')

        await wrapper.find('form').trigger('submit')
        await flushPromises()

        const postCall = apiMock.mock.calls.find(c => c[1]?.method === 'POST')
        expect(postCall).toBeDefined()
        const body = postCall![1]!.body as { url: string; events: string[] }
        expect(body.events).toHaveLength(8)
        expect(body.events).toContain('subscriber.newsletter_unsubscribed')
    })
})
