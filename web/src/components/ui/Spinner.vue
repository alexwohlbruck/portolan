<script setup lang="ts">
import { Loader2 } from 'lucide-vue-next'
import { cn } from '@/lib/utils'
const props = defineProps<{ class?: any }>()
</script>

<template><Loader2 :class="cn('animate-spin', props.class)" /></template>
