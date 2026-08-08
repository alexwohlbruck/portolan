<script setup lang="ts">
import { cva, type VariantProps } from 'class-variance-authority'
import { cn } from '@/lib/utils'

const badgeVariants = cva(
  'inline-flex items-center gap-1 rounded-md border px-2 py-0.5 text-xs font-medium transition-colors whitespace-nowrap',
  {
    variants: {
      variant: {
        default: 'border-transparent bg-primary text-primary-foreground',
        secondary: 'border-transparent bg-secondary text-secondary-foreground',
        outline: 'text-foreground border-border',
        success: 'border-transparent bg-[var(--success)]/15 text-[var(--success)]',
        warning: 'border-transparent bg-[var(--warning)]/15 text-[var(--warning)]',
        info: 'border-transparent bg-[var(--info)]/15 text-[var(--info)]',
        destructive: 'border-transparent bg-destructive/15 text-destructive',
        muted: 'border-transparent bg-muted text-muted-foreground',
      },
    },
    defaultVariants: { variant: 'default' },
  },
)
type Props = VariantProps<typeof badgeVariants>
const props = defineProps<{ variant?: Props['variant']; class?: any }>()
</script>

<template>
  <span :class="cn(badgeVariants({ variant: props.variant }), props.class)"><slot /></span>
</template>
