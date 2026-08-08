<script setup lang="ts">
import { cn } from '@/lib/utils'
const props = defineProps<{ class?: any }>()
</script>

<template>
  <div :class="cn('p-5 pt-0', props.class)"><slot /></div>
</template>
