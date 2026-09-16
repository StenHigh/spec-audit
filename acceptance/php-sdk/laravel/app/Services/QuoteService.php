<?php

declare(strict_types=1);

namespace App\Services;

use App\Contracts\QuoteContract;

final class QuoteService implements QuoteContract
{
    public function quote(int $amount): int
    {
        return $amount * 2;
    }
}
