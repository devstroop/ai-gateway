import { useQuery } from '@tanstack/react-query'
import { Button } from '@astryxdesign/core/Button'
import { Card } from '@astryxdesign/core/Card'
import { Heading } from '@astryxdesign/core/Heading'
import { Badge } from '@astryxdesign/core/Badge'
import { HStack } from '@astryxdesign/core/HStack'
import { VStack } from '@astryxdesign/core/VStack'
import { apiFetch } from '../lib/api'

type Model = { id: string; object: string; owned_by: string }

export default function Dashboard() {
  const health = useQuery({
    queryKey: ['health'],
    queryFn: () => apiFetch<{ status: string }>('/health'),
  })
  const models = useQuery({
    queryKey: ['models'],
    queryFn: () => apiFetch<{ object: string; data: Model[] }>('/v1/models'),
  })

  return (
    <VStack gap={6}>
      <HStack justify="between" align="center">
        <Heading level={1}>Dashboard</Heading>
        <Badge
          variant={health.data?.status === 'ok' ? 'success' : 'neutral'}
          label={health.isLoading ? 'checking…' : health.data?.status ?? 'unknown'}
        />
      </HStack>

      <Card>
        <VStack gap={3}>
          <Heading level={2}>Available models</Heading>
          {models.isLoading && <p>Loading…</p>}
          {models.isError && <p>Failed: {(models.error as Error).message}</p>}
          {models.data && (
            <VStack gap={2}>
              {models.data.data.map((m) => (
                <HStack key={m.id} justify="between" align="center">
                  <span>{m.id}</span>
                  <Badge variant="neutral" label={m.owned_by} />
                </HStack>
              ))}
              {models.data.data.length === 0 && <p>No models advertised.</p>}
            </VStack>
          )}
        </VStack>
      </Card>

      <HStack gap={3}>
        <Button label="Refresh health" onClick={() => health.refetch()} />
        <Button label="Refresh models" variant="secondary" onClick={() => models.refetch()} />
      </HStack>
    </VStack>
  )
}
